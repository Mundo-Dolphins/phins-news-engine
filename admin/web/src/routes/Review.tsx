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
        <a class="card review-card" href="/review/dataset">
          <span class="review-card-title">Dataset review</span>
          <span class="muted">
            Relabel or confirm the audited rows of the training dataset in a reviewed copy (shortcuts P, R, C, S).
          </span>
        </a>
        <a class="card review-card" href="/review/entities">
          <span class="review-card-title">Entity review</span>
          <span class="muted">
            Approve, reject or merge the pending entity proposals into the dictionary and entity catalog (shortcuts A,
            R, S; N and M for a possible duplicate).
          </span>
        </a>
        <a class="card review-card" href="/review/events">
          <span class="review-card-title">Event rule review</span>
          <span class="muted">
            Discover event rule phrases, accept, reject or edit them against the sample, then gate and apply the
            approved ones (shortcuts A, R, E, S).
          </span>
        </a>
      </div>
    </LocalOnly>
  );
}
