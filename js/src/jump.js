// Skew-binary jump targets (SPEC §7).

/** J(n) in O(log n): n − J(n) is the smallest term of n's greedy decomposition into 2^k − 1. */
export function jump(n) {
  if (n <= 0) return 0;
  let r = n;
  for (;;) {
    let t = 1;
    while (t * 2 + 1 <= r) t = t * 2 + 1;
    if (t === r) return n - t;
    r -= t;
  }
}
