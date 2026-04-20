// Context + hook for the runtime UI config (SVOTE_UI_MODE-derived gates and
// the published voting-config). Kept in a non-component module so Vite Fast
// Refresh works correctly for the Provider in uiConfig.tsx.

import { createContext, useContext } from "react";
import * as chainApi from "../api/chain";

export interface UIConfigContextValue {
  uiMode: chainApi.UIMode;
  devPIRControls: boolean;
  /**
   * CDN bucket origin (no trailing slash) this svoted's PIR siblings
   * bootstrap from, resolved server-side from SVOTE_PRECOMPUTED_BASE_URL.
   * `null` until /api/ui-config has loaded, or if the server build pre-dates
   * exposing it.
   */
  precomputedBaseURL: string | null;
  publishedConfig: chainApi.VotingConfig | null;
  uiConfigLoaded: boolean;
  publishedConfigLoaded: boolean;
  refreshPublishedConfig: () => Promise<void>;
}

export const DEFAULT_UI_CONFIG: UIConfigContextValue = {
  uiMode: "prod",
  devPIRControls: false,
  precomputedBaseURL: null,
  publishedConfig: null,
  uiConfigLoaded: false,
  publishedConfigLoaded: false,
  refreshPublishedConfig: async () => {},
};

export const UIConfigCtx = createContext<UIConfigContextValue>(DEFAULT_UI_CONFIG);

export function useUIConfig(): UIConfigContextValue {
  return useContext(UIConfigCtx);
}
