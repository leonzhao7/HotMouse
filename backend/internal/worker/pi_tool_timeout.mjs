// Explicitly loaded by HotMouse. Limits foreground bash tools within a Step.
export default function registerBashTimeout(pi) {
  const configured = Number(process.env.HOTMOUSE_BASH_TIMEOUT_SECONDS ?? process.env.CAIRN_BASH_TIMEOUT_SECONDS ?? 90);
  const maximum = Number.isFinite(configured) && configured > 0 ? configured : 90;
  pi.on("tool_call", async (event) => {
    if (event.toolName !== "bash") return;
    const requested = event.input.timeout;
    event.input.timeout = typeof requested === "number" && Number.isFinite(requested) && requested > 0
      ? Math.min(requested, maximum) : maximum;
  });
}
