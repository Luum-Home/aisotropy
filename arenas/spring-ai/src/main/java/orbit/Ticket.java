package orbit;

/** Structured-output target for the BeanOutputConverter demo (a support-ticket classification). */
public record Ticket(String summary, String category, int priority) {
}
