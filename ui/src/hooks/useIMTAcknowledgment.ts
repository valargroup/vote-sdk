import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";

// An acknowledgment belongs to one displayed context. A captured guard also
// expires when the checkbox is cleared/rechecked while a wallet popup is open.
export function useIMTAcknowledgment(context: string) {
  const [state, setState] = useState({ context, checked: false });
  if (state.context !== context) setState({ context, checked: false });
  const checked = state.context === context && state.checked;
  const live = useRef({ context, checked: false });
  useLayoutEffect(() => {
    live.current = { context, checked };
    return () => { live.current = { context: "", checked: false }; };
  }, [context, checked]);
  const setChecked = useCallback((value: boolean) => setState({ context, checked: value }), [context]);
  useEffect(() => {
    const reset = () => {
      live.current = { context: "", checked: false };
      setChecked(false);
    };
    window.addEventListener("keplr_keystorechange", reset);
    return () => window.removeEventListener("keplr_keystorechange", reset);
  }, [setChecked]);
  const capture = useCallback(() => {
    const acknowledged = live.current;
    const assertCurrent = () => {
      if (!acknowledged.checked || live.current !== acknowledged) {
        throw new Error("Acknowledge IMT root verification for the current round and wallet before continuing.");
      }
    };
    assertCurrent();
    return assertCurrent;
  }, []);
  return { checked, setChecked, capture };
}
