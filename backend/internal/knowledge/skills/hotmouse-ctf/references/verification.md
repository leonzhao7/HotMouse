# Verify the claim, preserve the Step boundary

Use this reference when the assigned Step explicitly verifies a result or
needs minimum confirmation before returning a Finding. Verification uses the
ordinary HotMouse execution response; do not return sapper's `verified/issues`
schema or initiate another worker.

- Vulnerability: inspect the saved proof request and response, verify the
  claimed behavior and its target. A scanner warning or version string alone
  is not proof. Use the minimum bounded check if saved evidence is insufficient.
- Credential: a recorded successful scoped authentication is evidence. If the
  Step requires a new check, one authentication attempt is usually enough;
  do not broaden it into a password spray.
- Flag: confirm the exact value occurs in the recorded target/artifact
  evidence and matches the challenge context. A placeholder, echoed payload
  or old-project flag is not capture evidence. Do not resubmit during a
  verification Step. Platform acceptance requires an actual adapter response
  or reconciliation result.
- Offline solution: inspect the input, transform/solver, output and format.
  Save enough information to reproduce the result without repeating unrelated
  extraction or expensive search.

Return `progress` with the verified observation, `negative` with a disproved
claim, or the relevant blocked/environment/no-information result when a check
cannot establish it. State the evidence path and remaining uncertainty.
Recovery summaries are separate: read saved session evidence and perform no
new actions or verification requests.
