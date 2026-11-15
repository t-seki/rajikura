/** Badge は既定で文字を 1 行に収めて「…」で切る。狭いときは折り返して全文を出す（高さは文字に合わせて増える）。 */
export const wrapBadgeStyles = {
  root: { height: "auto", minHeight: "var(--badge-height)", borderRadius: "calc(var(--badge-height) / 2)" },
  label: { whiteSpace: "normal", textOverflow: "clip", overflow: "visible" },
} as const;
