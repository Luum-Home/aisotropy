package arena.springai;

import java.util.ArrayList;
import java.util.List;

import org.springframework.ai.document.Document;
import org.springframework.ai.embedding.Embedding;
import org.springframework.ai.embedding.EmbeddingModel;
import org.springframework.ai.embedding.EmbeddingRequest;
import org.springframework.ai.embedding.EmbeddingResponse;

/**
 * Offline {@link EmbeddingModel} used when {@code ARENA_STUB=1}: a deterministic
 * 384-dim unit vector derived from the input text (no network call to the e5
 * server). The dimension matches the real embedder so downstream shapes line
 * up; the values are NOT semantically meaningful — the stub lane is a wiring
 * smoke lane, not a retrieval-quality lane.
 */
final class StubEmbeddingModel implements EmbeddingModel {

    private static final int DIM = 384;

    @Override
    public EmbeddingResponse call(EmbeddingRequest request) {
        List<Embedding> out = new ArrayList<>();
        List<String> inputs = request.getInstructions();
        for (int i = 0; i < inputs.size(); i++) {
            out.add(new Embedding(vector(inputs.get(i)), i));
        }
        return new EmbeddingResponse(out);
    }

    @Override
    public float[] embed(Document document) {
        return vector(document.getText());
    }

    private static float[] vector(String text) {
        float[] v = new float[DIM];
        long seed = 1469598103934665603L;
        if (text != null) {
            for (int i = 0; i < text.length(); i++) {
                seed ^= text.charAt(i);
                seed *= 1099511628211L;
            }
        }
        double norm = 0.0;
        for (int i = 0; i < DIM; i++) {
            seed ^= (seed << 13);
            seed ^= (seed >>> 7);
            seed ^= (seed << 17);
            float x = (seed % 1000) / 1000.0f;
            v[i] = x;
            norm += (double) x * x;
        }
        norm = Math.sqrt(norm);
        if (norm > 0) {
            for (int i = 0; i < DIM; i++) {
                v[i] = (float) (v[i] / norm);
            }
        }
        return v;
    }
}
