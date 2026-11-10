import templates from '../../internal/webui/suggested_commands.json' with { type: 'json' };

// Keep every built-in suggestion in the registry checked against the CLI.
export function suggestedCommand(name: keyof typeof templates, values: Record<string, string | number> = {}): string {
  return templates[name].replace(/\b[A-Z]+\b/g, key => String(values[key] ?? key));
}
