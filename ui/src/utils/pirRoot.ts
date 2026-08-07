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
  layoutDepth?: number;
  layoutKey?: string;
  layoutLabel?: string;
}

export function normalizePirRoot(root: PirRootResponse): NormalizedPirRoot {
  const layout = root.pir_layout;
  const layoutDepth = layout?.pir_depth ?? root.pir_depth;
  const layoutKey = layout
    ? `${layout.pir_depth}:${layout.tier0_layers}:${layout.tier1_layers}`
    : undefined;
  const layoutLabel = layout
    ? `${layout.pir_depth} (${layout.tier0_layers}+${layout.tier1_layers})`
    : root.pir_depth?.toString();

  return {
    pirRoot: root.pir_root ?? root.root25,
    circuitRoot: root.circuit_root ?? root.root29,
    layoutDepth,
    layoutKey,
    layoutLabel,
  };
}

/** Compare known depths and only compare tier splits reported by both replicas. */
export function pirLayoutsDiverge(roots: NormalizedPirRoot[]): boolean {
  const depths = new Set(
    roots
      .map((root) => root.layoutDepth)
      .filter((depth): depth is number => depth !== undefined)
  );
  if (depths.size > 1) return true;

  const detailedLayouts = new Set(
    roots
      .map((root) => root.layoutKey)
      .filter((layout): layout is string => layout !== undefined)
  );
  return detailedLayouts.size > 1;
}
