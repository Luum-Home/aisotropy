// guest.rs is the constant-work WASM fixture for the wasm-vs-docker benchmark,
// compiled with the Rust toolchain (`rustc --target wasm32-wasip1`). It is the
// LEAN counterpart to the Go/wasip1 guest (testdata/guest), which bundles the
// whole Go runtime (3+ MiB). This guest is `#![no_std]`, dependency-free (no
// serde, no alloc crate, no libc), and panics as `unreachable` — so the emitted
// module imports NOTHING and carries no runtime, which is exactly the "leaner
// guest" hypothesis the benchmark exists to measure.
//
// It matches the aisotropy WASM tool ABI (see internal/wasmtool):
//   - exports linear memory (rustc exports `memory` for wasm targets by default)
//   - aisotropy_alloc(size: u32) -> ptr: u32   — hand off a region of the arena
//   - aisotropy_run(ptr: u32, len: u32) -> u64 — read JSON args, return the
//     packed (ptr<<32)|len of the JSON result inside linear memory
//
// The semantic work is held constant with the Go/TinyGo guests: read {"name","n"}
// and return {"greeting":"hello <name>","doubled":<n*2>}. Because the module is
// re-instantiated fresh per Dispatch (wazero WithName("") + Close), a static bump
// arena is safe: each dispatch starts from a zeroed instance of linear memory.
#![no_std]
#![allow(static_mut_refs)]

use core::panic::PanicInfo;

#[panic_handler]
fn panic(_: &PanicInfo) -> ! {
    // no_std + panic=abort: lower to an unreachable trap, no unwinding runtime.
    core::arch::wasm32::unreachable()
}

// A fixed bump arena. One WebAssembly page (64 KiB) is far more than the tiny
// echo payloads need, and it lives in the module's data segment — no allocator,
// no free list, no host imports. Reset implicitly every Dispatch because wazero
// instantiates a fresh module (fresh linear memory) for each call.
const ARENA: usize = 1 << 16;
static mut MEM: [u8; ARENA] = [0u8; ARENA];
static mut NEXT: usize = 0;

#[no_mangle]
pub extern "C" fn aisotropy_alloc(size: u32) -> u32 {
    let sz = if size == 0 { 1 } else { size as usize };
    unsafe {
        let off = NEXT;
        // Saturate rather than wrap: an over-large request just clamps to the
        // arena end; the host's mem.Write bounds-check then rejects it cleanly.
        NEXT = core::cmp::min(off + sz, ARENA);
        (MEM.as_ptr() as usize + off) as u32
    }
}

// find_str_value returns the byte slice of the string value for `key` in a flat
// JSON object, or an empty slice if absent. Intentionally minimal (no nesting,
// no escapes) — sufficient for the fixed {"name":...,"n":...} benchmark input,
// and the parse cost is deliberately trivial to keep the benchmark measuring the
// isolation mechanism, not the guest's own compute.
fn find_str_value(input: &[u8], key: &[u8]) -> (usize, usize) {
    if let Some(k) = find(input, key) {
        // advance past key + closing quote + ':' + opening quote
        let mut i = k + key.len();
        while i < input.len() && input[i] != b':' {
            i += 1;
        }
        i += 1; // past ':'
        while i < input.len() && input[i] != b'"' {
            i += 1;
        }
        i += 1; // past opening quote
        let start = i;
        while i < input.len() && input[i] != b'"' {
            i += 1;
        }
        return (start, i);
    }
    (0, 0)
}

// find_int_value parses the integer value for `key` (unquoted JSON number).
fn find_int_value(input: &[u8], key: &[u8]) -> i64 {
    if let Some(k) = find(input, key) {
        let mut i = k + key.len();
        while i < input.len() && input[i] != b':' {
            i += 1;
        }
        i += 1;
        while i < input.len() && (input[i] == b' ' || input[i] == b'"') {
            i += 1;
        }
        let mut neg = false;
        if i < input.len() && input[i] == b'-' {
            neg = true;
            i += 1;
        }
        let mut v: i64 = 0;
        while i < input.len() && input[i].is_ascii_digit() {
            v = v * 10 + (input[i] - b'0') as i64;
            i += 1;
        }
        return if neg { -v } else { v };
    }
    0
}

// find locates the byte offset of `needle` in `hay` (naive scan). The keys are
// searched as `"key"` so we match the JSON field name, not a value substring.
fn find(hay: &[u8], needle: &[u8]) -> Option<usize> {
    if needle.is_empty() || needle.len() > hay.len() {
        return None;
    }
    let mut i = 0;
    while i + needle.len() <= hay.len() {
        // Explicit byte loop rather than slice `==`, which lowers to an
        // `env.memcmp` import the sandbox capability gate would reject (the guest
        // is only granted WASI, and here not even that). This keeps the module
        // import-free.
        let mut j = 0;
        while j < needle.len() && hay[i + j] == needle[j] {
            j += 1;
        }
        if j == needle.len() {
            return Some(i);
        }
        i += 1;
    }
    None
}

#[no_mangle]
pub extern "C" fn aisotropy_run(ptr: u32, length: u32) -> u64 {
    // Copy the name out of the input region into a stack buffer BEFORE writing
    // any output, so we never hold a shared borrow of MEM while also mutating it
    // through the Writer (that aliasing would be undefined behaviour in Rust,
    // even though the two regions don't physically overlap here).
    let mut name_buf = [0u8; 256];
    let mut name_len = 0usize;
    let n;
    unsafe {
        let base = MEM.as_ptr() as usize;
        let off = ptr as usize - base;
        let input = &MEM[off..off + length as usize];
        let (ns, ne) = find_str_value(input, b"\"name\"");
        let mut k = ns;
        while k < ne && name_len < name_buf.len() {
            name_buf[name_len] = input[k];
            name_len += 1;
            k += 1;
        }
        n = find_int_value(input, b"\"n\"");
    }

    // Build the result JSON: {"greeting":"hello <name>","doubled":<n*2>} into a
    // fresh region of the arena (past whatever aisotropy_alloc handed out), then
    // return its packed ptr/len.
    let out_off = unsafe { NEXT };
    let mut w = Writer::new(out_off);
    w.put(b"{\"greeting\":\"hello ");
    w.put(&name_buf[..name_len]);
    w.put(b"\",\"doubled\":");
    w.put_int(n * 2);
    w.put(b"}");
    let (rptr, rlen) = w.finish();
    ((rptr as u64) << 32) | (rlen as u64)
}

// Writer appends bytes into the static arena starting at a byte offset, tracking
// how many bytes were written so aisotropy_run can return the packed ptr/len.
struct Writer {
    start: usize,
    pos: usize,
}

impl Writer {
    fn new(start: usize) -> Writer {
        Writer { start, pos: start }
    }
    fn put(&mut self, b: &[u8]) {
        unsafe {
            for &c in b {
                if self.pos < ARENA {
                    MEM[self.pos] = c;
                    self.pos += 1;
                }
            }
            NEXT = self.pos;
        }
    }
    fn put_int(&mut self, mut v: i64) {
        if v == 0 {
            self.put(b"0");
            return;
        }
        let mut buf = [0u8; 20];
        let mut i = buf.len();
        let neg = v < 0;
        if neg {
            v = -v;
        }
        while v > 0 {
            i -= 1;
            buf[i] = b'0' + (v % 10) as u8;
            v /= 10;
        }
        if neg {
            i -= 1;
            buf[i] = b'-';
        }
        self.put(&buf[i..]);
    }
    fn finish(&self) -> (u32, u32) {
        unsafe {
            let base = MEM.as_ptr() as usize;
            ((base + self.start) as u32, (self.pos - self.start) as u32)
        }
    }
}
