// Provider for the runtime UI config: SVOTE_UI_MODE-derived gates from svoted
// (/api/ui-config) and the published voting-config (/api/voting-config).
//
// Both are fetched once at app startup and re-exposed through a context so any
// component can branch on them without re-fetching. On fetch failure the
// defaults are deliberately conservative — prod-mode + null published config —
// so an older svoted or an offline CDN can never widen the surface.

import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import * as chainApi from "../api/chain";
import {
  UIConfigCtx,
  type UIConfigContextValue,
} from "./uiConfigContext";

export function UIConfigProvider({ children }: { children: ReactNode }) {
  const [uiConfig, setUIConfig] = useState<chainApi.UIConfig | null>(null);
  const [uiConfigLoaded, setUIConfigLoaded] = useState(false);
  const [publishedConfig, setPublishedConfig] =
    useState<chainApi.VotingConfig | null>(null);
  const [publishedConfigLoaded, setPublishedConfigLoaded] = useState(false);

  // Initial /api/ui-config fetch. The cancelled flag protects against the
  // StrictMode double-invoke unmount.
  useEffect(() => {
    let cancelled = false;
    chainApi.getUIConfig().then((cfg) => {
      if (cancelled) return;
      setUIConfig(cfg);
      setUIConfigLoaded(true);
    });
    return () => {
      cancelled = true;
    };
  }, []);

  const refreshPublishedConfig = useCallback(async () => {
    const cfg = await chainApi.getVotingConfig();
    setPublishedConfig(cfg);
    setPublishedConfigLoaded(true);
  }, []);

  // Initial /api/voting-config fetch. Inlined so the eslint
  // `react-hooks/set-state-in-effect` rule sees the call shape directly.
  useEffect(() => {
    let cancelled = false;
    chainApi.getVotingConfig().then((cfg) => {
      if (cancelled) return;
      setPublishedConfig(cfg);
      setPublishedConfigLoaded(true);
    });
    return () => {
      cancelled = true;
    };
  }, []);

  const value = useMemo<UIConfigContextValue>(
    () => ({
      uiMode: uiConfig?.mode ?? "prod",
      devPIRControls: uiConfig?.dev_pir_controls ?? false,
      precomputedBaseURL: uiConfig?.precomputed_base_url ?? null,
      publishedConfig,
      uiConfigLoaded,
      publishedConfigLoaded,
      refreshPublishedConfig,
    }),
    [uiConfig, publishedConfig, uiConfigLoaded, publishedConfigLoaded, refreshPublishedConfig]
  );

  return <UIConfigCtx.Provider value={value}>{children}</UIConfigCtx.Provider>;
}
