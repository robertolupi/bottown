// The keyboard (monitor-ui design §5). App owns the one keydown
// listener and asks keyIntent what a key means, so every key is handled
// in one place rather than by the component it acts on. A key is ignored
// while a field has focus or a command modifier is held, so text entry
// and the browser's own shortcuts win.
import { VIEWS, type View } from './routes';

export type KeyIntent =
  | { kind: 'view'; view: View }
  | { kind: 'move'; delta: number }
  | { kind: 'open' }
  | { kind: 'back' }
  | { kind: 'follow' }
  | { kind: 'log-top' }
  | { kind: 'log-end' }
  | { kind: 'search' }
  | { kind: 'help' };

// A field that takes the keys for itself.
export function isEditableTarget(target: EventTarget | null): boolean {
  return Boolean(
    target instanceof HTMLInputElement ||
      target instanceof HTMLTextAreaElement ||
      target instanceof HTMLSelectElement ||
      (target instanceof HTMLElement && target.isContentEditable),
  );
}

// Ctrl, Alt and Meta belong to the browser; Shift does not, because `G`
// and `?` need it.
export function hasCommandModifier(e: KeyboardEvent): boolean {
  return e.ctrlKey || e.metaKey || e.altKey;
}

// keyIntent maps a keydown to what the UI does, or null to leave it
// alone. onRunFlowPage turns on the log keys, which only a run or flow
// page has.
export function keyIntent(e: KeyboardEvent, onRunFlowPage: boolean): KeyIntent | null {
  if (isEditableTarget(e.target) || hasCommandModifier(e)) return null;
  const digit = Number(e.key);
  if (Number.isInteger(digit) && digit >= 1 && digit <= VIEWS.length) {
    return { kind: 'view', view: VIEWS[digit - 1].view };
  }
  switch (e.key) {
    case 'j':
    case 'ArrowDown':
      return { kind: 'move', delta: 1 };
    case 'k':
    case 'ArrowUp':
      return { kind: 'move', delta: -1 };
    case 'Enter':
      // A focused link activates itself; tiles and rows need us.
      return e.target instanceof HTMLAnchorElement ? null : { kind: 'open' };
    case 'Escape':
      return { kind: 'back' };
    case '/':
      return { kind: 'search' };
    case '?':
      return { kind: 'help' };
    case 'f':
      return onRunFlowPage ? { kind: 'follow' } : null;
    case 'g':
      return onRunFlowPage ? { kind: 'log-top' } : null;
    case 'G':
      return onRunFlowPage ? { kind: 'log-end' } : null;
    default:
      return null;
  }
}

// What the `?` panel lists (design §5), in one place so the panel cannot
// drift from the keys.
export const KEY_HELP: { keys: string; does: string }[] = [
  { keys: '1-6', does: 'Switch view' },
  { keys: 'j / k, arrows', does: 'Move the selection' },
  { keys: 'Enter', does: 'Open the selected item' },
  { keys: 'Esc', does: 'Back' },
  { keys: 'f', does: 'Follow or stop following the log' },
  { keys: 'g / G', does: 'Top or end of the log' },
  { keys: '/', does: "Focus History's search" },
  { keys: '?', does: 'Show these keys' },
];

// The run and flow page registers how its log reacts to f/g/G while it is
// mounted, so the one listener can drive it without owning its log state.
export interface LogCommands {
  toggleFollow(): void;
  toTop(): void;
  toEnd(): void;
}

let logCommands: LogCommands | null = null;

export function setLogCommands(commands: LogCommands | null): void {
  logCommands = commands;
}

export function getLogCommands(): LogCommands | null {
  return logCommands;
}
