export default function StepResponse({ step }) {
  if (!step.llm_response_recorded) {
    return (
      <span className="muted">
        {step.to || step.concluded_at ? "未保存原始返回" : "尚未返回"}
      </span>
    );
  }
  const response = step.llm_response || "";
  return (
    <div className="step-response">
      {response ? (
        <>
          <p className="step-response-preview">{response}</p>
          <details className="step-response-detail">
            <summary>查看完整返回</summary>
            <pre>{response}</pre>
          </details>
        </>
      ) : (
        <span className="muted">返回为空</span>
      )}
      {step.llm_response_error && (
        <p className="step-response-error">{step.llm_response_error}</p>
      )}
    </div>
  );
}
