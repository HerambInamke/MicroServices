// Trust score (0-100), see tasks/phase5-plan.md:
// a Bayesian-smoothed rating (prior mean M over C phantom reviews) blended with a
// Laplace-smoothed delivered ratio, so a single 5-star review or order cannot give 100.
const M = 3;
const C = 5;

function trustScore({ reviewCount, ratingSum, delivered, cancelled }) {
  const rating = (C * M + ratingSum) / (C + reviewCount);
  const fulfil = (delivered + 1) / (delivered + cancelled + 2);
  return Math.round(100 * (0.7 * ((rating - 1) / 4) + 0.3 * fulfil));
}

module.exports = { trustScore };
