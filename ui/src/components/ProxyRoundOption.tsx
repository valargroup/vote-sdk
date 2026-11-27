import { PROXY_APPROVAL_FLOOR } from "../api/coordinatorActions";

interface ProxyRoundOptionProps {
  /** Whether the chain reports the proxy capability fields; see supportsProxyDelegation. */
  supported: boolean;
  checked: boolean;
  onChange: (checked: boolean) => void;
}

/**
 * The create-round flow's proxy delegation checkbox, which sets the session's
 * proxy flag. It renders only when the chain supports proxy delegation, and
 * says that the chain then waits for the approval floor.
 */
export function ProxyRoundOption({ supported, checked, onChange }: ProxyRoundOptionProps) {
  if (!supported) return null;
  return (
    <label className="flex items-start gap-2 rounded-lg bg-surface-2 border border-border-subtle p-3 cursor-pointer">
      <input
        type="checkbox"
        checked={checked}
        onChange={(e) => onChange(e.target.checked)}
        className="mt-0.5 shrink-0"
      />
      <span>
        <span className="block text-[11px] font-semibold text-text-primary">Proxy delegation round</span>
        <span className="block text-[10px] text-text-muted mt-0.5">
          Voters can delegate their weight to public delegates in this round. The chain creates it only
          after at least {PROXY_APPROVAL_FLOOR} coordinator approvals, or the threshold if that is higher, so
          proposing alone never creates it.
        </span>
      </span>
    </label>
  );
}
