export interface PirLayout {
  pir_depth: number;
  tier0_layers: number;
  tier1_layers: number;
  poly_len: number;
}

export interface PirRootResponse {
  height: number | null;
  num_ranges: number;
  /** Selects the authoritative root field names for this dataset contract. */
  dataset_version: number;
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
    ? `${layout.pir_depth}:${layout.tier0_layers}:${layout.tier1_layers}:${layout.poly_len}`
    : undefined;
  const layoutLabel = layout
    ? `${layout.pir_depth} (${layout.tier0_layers}+${layout.tier1_layers}) · poly_len ${layout.poly_len}`
    : root.pir_depth?.toString();
  let pirRoot: string | undefined;
  let circuitRoot: string | undefined;
  if (root.dataset_version === 1) {
    pirRoot = root.root25;
    circuitRoot = root.root29;
  } else if (root.dataset_version === 2) {
    pirRoot = root.pir_root;
    circuitRoot = root.circuit_root;
  }

  return {
    pirRoot,
    circuitRoot,
    layoutDepth,
    layoutKey,
    layoutLabel,
  };
}

/** Compare known depths and detailed layouts only when replicas report them. */
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
