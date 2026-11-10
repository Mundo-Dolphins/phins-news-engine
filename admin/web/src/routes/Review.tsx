import { LocalOnly } from "../components/LocalOnly";

/** Review lists the available review screens. */
export function Review() {
  return (
    <LocalOnly>
      <h1>Review</h1>
      <p class="muted">Interactive reviews of queued decisions. Each decision is saved as soon as it is made.</p>
      <div class="review-index">
        <a class="card review-card" href="/review/train">
          <span class="review-card-title">Train review</span>
          <span class="muted">Publish or discard the LLM teacher's proposals as human labels (shortcuts P, D, S).</span>
        </a>
      </div>
    </LocalOnly>
  );
}
