"""
bet02_lm_arithmetic_codec.py — Bet 2: working memory as a codec.

Minimum experiment for docs/research/bet-02-working-memory-codec.md:
compress a realistic multi-turn chat history by driving an arithmetic coder
with a *local LM's* next-token probability distribution — the exact
nncp/ts_zip (Bellard) technique — then reconstruct byte-for-byte and compare
bits/char against gzip/bzip2/xz and the < 1 bit/char target.

Pipeline (self-contained CLI, no heavy new deps — the arithmetic coder is
~120 lines of pure Python here):

    encode  -> feed text through LM, per-token distribution drives the coder
    measure -> total bits, bits/char, bits/byte
    decode  -> reconstruct from the bitstream using the SAME LM
    verify  -> assert decoded == original, byte-for-byte (losslessness)
    compare -> gzip / bzip2 / xz bits/char on the same text

--------------------------------------------------------------------------
Why this is lossless (the determinism contract)
--------------------------------------------------------------------------
Arithmetic coding is exact only if encoder and decoder see the EXACT SAME
probability model at every step. The model here is the LM's next-token
distribution, quantized to an integer frequency table. To guarantee the
float logits are bit-identical on both sides we run the model through ONE
shared function `next_logits(prefix)` that both encode and decode call with
the identical token prefix (same CPU, eval mode, no_grad, float32 forward,
float64 quantization, same bounded context window). Encode does not use a
single teacher-forced forward pass (whose per-position logits can differ in
the last ULPs from an incremental forward and silently break losslessness);
it recomputes each step exactly as decode does. Same inputs -> same logits
-> same integer freq table -> reversible coding.

Determinism knobs: CPU only, torch.no_grad, model.eval(), float64
softmax/quantization, fixed integer SCALE. A single mismatched bit anywhere
breaks the round-trip, so the byte-equality assertion at the end is the real
test — not a formality.
"""

from __future__ import annotations

import argparse
import bz2
import gzip
import lzma
import os
import sys
import time

# --------------------------------------------------------------------------
# Arithmetic coder — textbook 32-bit Witten-Neal-Cleary (WNC) integer coder.
# Total frequency per step must stay <= 2^30 (=FIRST_QTR); we use SCALE=2^24,
# comfortably below that bound.
# --------------------------------------------------------------------------

CODE_BITS = 32
TOP = (1 << CODE_BITS) - 1
FIRST_QTR = (TOP + 1) // 4          # 2^30
HALF = 2 * FIRST_QTR                # 2^31
THIRD_QTR = 3 * FIRST_QTR           # 3*2^30

SCALE_BITS = 24
SCALE = 1 << SCALE_BITS             # target total frequency (2^24)


class ArithmeticEncoder:
    def __init__(self) -> None:
        self.low = 0
        self.high = TOP
        self.pending = 0
        self.bits = bytearray()

    def _emit(self, bit: int) -> None:
        self.bits.append(bit)
        while self.pending > 0:
            self.bits.append(bit ^ 1)
            self.pending -= 1

    def encode(self, cum_low: int, cum_high: int, total: int) -> None:
        rng = self.high - self.low + 1
        self.high = self.low + (rng * cum_high) // total - 1
        self.low = self.low + (rng * cum_low) // total
        while True:
            if self.high < HALF:
                self._emit(0)
            elif self.low >= HALF:
                self._emit(1)
                self.low -= HALF
                self.high -= HALF
            elif self.low >= FIRST_QTR and self.high < THIRD_QTR:
                self.pending += 1
                self.low -= FIRST_QTR
                self.high -= FIRST_QTR
            else:
                break
            self.low = 2 * self.low
            self.high = 2 * self.high + 1

    def finish(self) -> bytearray:
        self.pending += 1
        if self.low < FIRST_QTR:
            self._emit(0)
        else:
            self._emit(1)
        return self.bits


class ArithmeticDecoder:
    def __init__(self, bits: bytearray) -> None:
        self.bits = bits
        self.pos = 0
        self.low = 0
        self.high = TOP
        self.code = 0
        for _ in range(CODE_BITS):
            self.code = (self.code << 1) | self._next_bit()

    def _next_bit(self) -> int:
        if self.pos < len(self.bits):
            b = self.bits[self.pos]
            self.pos += 1
            return b
        return 0  # pad past-the-end with zeros

    def target(self, total: int) -> int:
        rng = self.high - self.low + 1
        return ((self.code - self.low + 1) * total - 1) // rng

    def update(self, cum_low: int, cum_high: int, total: int) -> None:
        rng = self.high - self.low + 1
        self.high = self.low + (rng * cum_high) // total - 1
        self.low = self.low + (rng * cum_low) // total
        while True:
            if self.high < HALF:
                pass
            elif self.low >= HALF:
                self.low -= HALF
                self.high -= HALF
                self.code -= HALF
            elif self.low >= FIRST_QTR and self.high < THIRD_QTR:
                self.low -= FIRST_QTR
                self.high -= FIRST_QTR
                self.code -= FIRST_QTR
            else:
                break
            self.low = 2 * self.low
            self.high = 2 * self.high + 1
            self.code = (self.code << 1) | self._next_bit()


def pack_bits(bits: bytearray) -> bytes:
    """Pack a 0/1 bit list into bytes (MSB first). This is the real stored
    artifact; its length in bytes*8 is the honest on-disk bit cost."""
    out = bytearray()
    acc = 0
    n = 0
    for b in bits:
        acc = (acc << 1) | b
        n += 1
        if n == 8:
            out.append(acc)
            acc = 0
            n = 0
    if n:
        out.append(acc << (8 - n))
    return bytes(out)


# --------------------------------------------------------------------------
# LM probability model
# --------------------------------------------------------------------------

def load_model(model_name: str):
    import torch  # noqa: F401
    from transformers import AutoTokenizer, AutoModelForCausalLM

    hf_cache = os.path.join(os.path.dirname(os.path.abspath(__file__)), ".hf_cache")
    os.environ.setdefault("HF_HOME", hf_cache)
    os.environ.setdefault("TOKENIZERS_PARALLELISM", "false")
    tok = AutoTokenizer.from_pretrained(model_name)
    model = AutoModelForCausalLM.from_pretrained(model_name)
    model.eval()
    return tok, model


def make_freq_fn(torch):
    """Returns a function logits(float64 tensor, V) -> (freq LongTensor, cum LongTensor, total int).
    Quantization is deterministic: freq_i = floor(p_i*(SCALE-V)) + 1 so every
    token has freq >= 1 (decodable) and total <= SCALE <= FIRST_QTR."""

    def freqs(logits_f64, vocab):
        probs = torch.softmax(logits_f64, dim=-1)  # float64
        freq = torch.floor(probs * (SCALE - vocab)).to(torch.int64) + 1
        cum = torch.cumsum(freq, dim=0)  # cumulative high edges
        total = int(cum[-1].item())
        return freq, cum, total

    return freqs


def next_logits(torch, model, prefix_ids, window):
    """The SHARED forward path — encode and decode both call this with the
    same prefix, guaranteeing bit-identical logits (the losslessness contract)."""
    ids = prefix_ids[-window:]
    inp = torch.tensor([ids], dtype=torch.long)
    with torch.no_grad():
        out = model(inp)
    return out.logits[0, -1].to(torch.float64)


# --------------------------------------------------------------------------
# Encode / decode
# --------------------------------------------------------------------------

def encode(torch, tok, model, text, window, log=sys.stderr):
    freqs = make_freq_fn(torch)
    vocab = model.config.vocab_size
    bos = tok.bos_token_id if tok.bos_token_id is not None else tok.eos_token_id
    token_ids = tok(text, return_tensors=None)["input_ids"]
    seq = [bos] + token_ids
    enc = ArithmeticEncoder()
    t0 = time.time()
    for i in range(1, len(seq)):
        logits = next_logits(torch, model, seq[:i], window)
        freq, cum, total = freqs(logits, vocab)
        t = seq[i]
        cum_high = int(cum[t].item())
        cum_low = cum_high - int(freq[t].item())
        enc.encode(cum_low, cum_high, total)
        if i % 100 == 0:
            print(f"[encode] {i}/{len(seq)-1} tokens "
                  f"({(time.time()-t0):.1f}s)", file=log)
    bits = enc.finish()
    return bits, token_ids, len(seq)


def decode(torch, tok, model, bits, n_tokens, window, log=sys.stderr):
    freqs = make_freq_fn(torch)
    vocab = model.config.vocab_size
    bos = tok.bos_token_id if tok.bos_token_id is not None else tok.eos_token_id
    dec = ArithmeticDecoder(bits)
    seq = [bos]
    t0 = time.time()
    for i in range(1, n_tokens + 1):
        logits = next_logits(torch, model, seq, window)
        freq, cum, total = freqs(logits, vocab)
        tgt = dec.target(total)
        # smallest index whose cumulative-high edge > target
        t = int(torch.searchsorted(cum, torch.tensor(tgt, dtype=cum.dtype),
                                   right=True).item())
        cum_high = int(cum[t].item())
        cum_low = cum_high - int(freq[t].item())
        dec.update(cum_low, cum_high, total)
        seq.append(t)
        if i % 100 == 0:
            print(f"[decode] {i}/{n_tokens} tokens "
                  f"({(time.time()-t0):.1f}s)", file=log)
    return seq[1:]  # drop BOS


# --------------------------------------------------------------------------
# Chat history sample — realistic multi-turn Spanish user<->assistant.
# Spanish on purpose (aisotropy's target user talks in Spanish; matches the
# rest of this spike). ~40 turns of a support/dev conversation.
# --------------------------------------------------------------------------

CHAT_SAMPLE = """Usuario: Hola, buenas. Necesito ayuda para configurar el deploy de mi backend en producción.
Asistente: Hola, con gusto te ayudo. ¿En qué proveedor está corriendo tu backend y qué stack usás?
Usuario: Está en AWS, región São Paulo. Es un servicio en Go que expone una API HTTP.
Asistente: Perfecto. ¿Estás usando contenedores con ECS, o corre directo sobre una instancia EC2?
Usuario: Por ahora es una sola instancia EC2, pero quiero migrar a contenedores pronto.
Asistente: Entiendo. Para empezar, ¿tenés el binario compilado para linux/amd64 y un systemd unit que lo mantenga vivo?
Usuario: Tengo el binario pero no configuré systemd todavía. Lo levanto a mano con nohup.
Asistente: Eso explica por qué se cae cuando cierra la sesión SSH. Te paso un unit file de systemd para que arranque solo al bootear.
Usuario: Dale, me vendría bárbaro. ¿Qué necesito poner en ese archivo?
Asistente: Un ExecStart apuntando al binario, un Restart=always, y el usuario de servicio con permisos mínimos. También conviene un WorkingDirectory fijo.
Usuario: ¿Y las variables de entorno como la API key? No las quiero hardcodear.
Asistente: Nunca las pongas en el código. Usá un EnvironmentFile con permisos 600, o mejor AWS Secrets Manager y las inyectás al arrancar.
Usuario: Prefiero Secrets Manager. ¿Eso agrega mucha latencia en cada request?
Asistente: No, se leen una sola vez al iniciar el proceso, no en cada request. Las cacheás en memoria tras el primer fetch.
Usuario: Buenísimo. Otra cosa: el reporte de métricas lo necesito los lunes a las 8am. ¿Cómo lo automatizo?
Asistente: Un cron job o un EventBridge schedule que dispare un Lambda que arme el reporte y te lo mande por mail.
Usuario: El problema es que la base de datos de staging tuvo la contraseña rotada el 3 de marzo y algunos scripts quedaron rotos.
Asistente: Ahí está el bug entonces. Actualizá el secreto rotado en Secrets Manager y que los scripts lo lean de ahí, no de un archivo viejo.
Usuario: Tiene sentido. ¿Vos recomendás rotar contraseñas automáticamente o a mano?
Asistente: Automático con Secrets Manager rotation, cada 30 o 90 días, así no dependés de que alguien se acuerde de hacerlo.
Usuario: Ok. Voy a revisar los logs de AWS para entender qué pasó con el deploy de ayer que tardó un montón.
Asistente: Fijate en CloudWatch los tiempos de arranque del proceso y si hubo throttling o cold starts. ¿El deploy es blue-green o in-place?
Usuario: Es in-place, apago el viejo y prendo el nuevo. Sé que no es lo ideal.
Asistente: In-place te deja una ventana de downtime. Con un load balancer y dos targets podés hacer rolling sin cortar el servicio.
Usuario: ¿Un ALB alcanza para eso o necesito algo más?
Asistente: Un ALB con un target group y health checks alcanza para rolling básico. Registrás el nuevo, esperás healthy, desregistrás el viejo.
Usuario: Perfecto. Una última: quiero que las respuestas de mi propio asistente sean en tono informal, como vos me hablás.
Asistente: Eso lo definís en el system prompt: pedile explícitamente tono informal y en español rioplatense, con ejemplos del estilo que querés.
Usuario: Genial, muchas gracias por toda la ayuda, me destrabaste un montón de cosas.
Asistente: De nada, para eso estamos. Cualquier cosa con el systemd o el ALB, me escribís y seguimos. ¡Éxitos con el deploy!
"""


# --------------------------------------------------------------------------
# Baselines + reporting
# --------------------------------------------------------------------------

def baseline_bits(data: bytes):
    return {
        "gzip": len(gzip.compress(data, 9)) * 8,
        "bzip2": len(bz2.compress(data, 9)) * 8,
        "xz": len(lzma.compress(data, preset=9)) * 8,
    }


def main() -> int:
    ap = argparse.ArgumentParser(description="Bet 2 — LM+arithmetic-coder text codec")
    ap.add_argument("--model", default=os.environ.get("SURPRISAL_MODEL", "DeepESP/gpt2-spanish"))
    ap.add_argument("--window", type=int, default=512, help="context window (tokens)")
    ap.add_argument("--text-file", default=None, help="optional path to text to compress")
    args = ap.parse_args()

    if args.text_file:
        with open(args.text_file, "r", encoding="utf-8") as fh:
            text = fh.read()
    else:
        text = CHAT_SAMPLE

    data = text.encode("utf-8")
    n_chars = len(text)
    n_bytes = len(data)

    print(f"# Bet 2 — working memory as a codec (LM + arithmetic coder)")
    print(f"# model={args.model}  window={args.window}")
    print(f"# text: {n_chars} chars, {n_bytes} UTF-8 bytes, raw UTF-8 = 8.000 bits/byte")
    print()

    import torch
    torch.manual_seed(0)
    tok, model = load_model(args.model)
    print(f"# LM loaded: {args.model}, vocab={model.config.vocab_size}", file=sys.stderr)

    t0 = time.time()
    bits, token_ids, n_seq = encode(torch, tok, model, text, args.window)
    enc_secs = time.time() - t0
    packed = pack_bits(bits)
    lm_bits_stream = len(bits)          # raw arithmetic-coder output bits
    lm_bits_packed = len(packed) * 8    # honest on-disk size (byte-aligned)

    t1 = time.time()
    decoded_ids = decode(torch, tok, model, bits, len(token_ids), args.window)
    dec_secs = time.time() - t1

    # --- losslessness: byte-for-byte equality ---
    decoded_text = tok.decode(decoded_ids)
    tokens_equal = (decoded_ids == token_ids)
    text_equal = (decoded_text == text)
    bytes_equal = (decoded_text.encode("utf-8") == data)

    base = baseline_bits(data)

    print("## RESULTS")
    print(f"tokens encoded            : {len(token_ids)}")
    print(f"arithmetic-coder bits     : {lm_bits_stream}")
    print(f"packed (byte-aligned) bits: {lm_bits_packed}  ({len(packed)} bytes)")
    print()
    print("## bits/char (denominator = characters)")
    print(f"  raw UTF-8 (reference)   : {n_bytes*8 / n_chars:8.4f}  (bytes*8/char)")
    print(f"  gzip -9                 : {base['gzip'] / n_chars:8.4f}")
    print(f"  bzip2 -9                : {base['bzip2'] / n_chars:8.4f}")
    print(f"  xz -9                   : {base['xz'] / n_chars:8.4f}")
    print(f"  LM + arithmetic (stream): {lm_bits_stream / n_chars:8.4f}")
    print(f"  LM + arithmetic (packed): {lm_bits_packed / n_chars:8.4f}")
    print(f"  TARGET (bet-02)         : < 1.0000")
    print()
    print("## bits/byte (denominator = UTF-8 bytes)")
    print(f"  gzip -9                 : {base['gzip'] / n_bytes:8.4f}")
    print(f"  LM + arithmetic (packed): {lm_bits_packed / n_bytes:8.4f}")
    print()
    print("## LOSSLESSNESS (byte-for-byte roundtrip)")
    print(f"  token ids  match : {tokens_equal}")
    print(f"  decoded == text  : {text_equal}")
    print(f"  utf-8 bytes match: {bytes_equal}")
    print()

    # hard assertion — the real test
    ok = tokens_equal and text_equal and bytes_equal
    print("## VERDICT")
    lm_bpc = lm_bits_packed / n_chars
    beats_gzip = lm_bits_packed < base["gzip"]
    under_1 = lm_bpc < 1.0
    print(f"  lossless roundtrip      : {'YES' if ok else 'NO — BROKEN'}")
    print(f"  beats gzip              : {'YES' if beats_gzip else 'NO'} "
          f"({lm_bpc:.4f} vs {base['gzip']/n_chars:.4f} bits/char)")
    print(f"  under 1.0 bits/char     : {'YES' if under_1 else 'NO'} ({lm_bpc:.4f})")
    print(f"  encode {enc_secs:.1f}s / decode {dec_secs:.1f}s (CPU, window={args.window})")

    assert ok, "LOSSLESS ROUNDTRIP FAILED — encode/decode disagree (determinism broken)"
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
