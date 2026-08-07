export interface PirLayout {
  pir_depth: number;
  tier0_layers: number;
  tier1_layers: number;
}

export interface PirRootResponse {
  height: number | null;
  num_ranges: number;
  pir_root?: string;
  circuit_root?: string;
  root25?: string;
  root29?: string;
  pir_layout?: PirLayout;
  pir_depth?: number;
}

export interface NormalizedPirRoot {
  pirRoot?: string;
  circuitRoot?: string;
  layoutKey: string;
  layoutLabel?: string;
}

export function normalizePirRoot(root: PirRootResponse): NormalizedPirRoot {
  const layout = root.pir_layout;
  const layoutKey = layout
    ? `${layout.pir_depth}:${layout.tier0_layers}:${layout.tier1_layers}`
    : root.pir_depth === undefined
      ? ""
      : `depth:${root.pir_depth}`;
  const layoutLabel = layout
    ? `${layout.pir_depth} (${layout.tier0_layers}+${layout.tier1_layers})`
    : root.pir_depth?.toString();

  return {
    pirRoot: root.pir_root ?? root.root25,
    circuitRoot: root.circuit_root ?? root.root29,
    layoutKey,
    layoutLabel,
  };
}
