/** Text wordmark: no upstream logo artwork, and it scales with the layout. */
export function BrandLogo({ priority = false }: { priority?: boolean }) {
  return (
    <span
      className="wk-logo"
      data-priority={priority ? "high" : undefined}
      style={{
        display: "inline-flex",
        alignItems: "baseline",
        gap: 6,
        fontSize: 20,
        lineHeight: 1,
        fontWeight: 700,
        letterSpacing: "0.02em",
        whiteSpace: "nowrap",
      }}
    >
      有临
      <span style={{ color: "#007ac3" }}>YLKnow</span>
    </span>
  );
}
