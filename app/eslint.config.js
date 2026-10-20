import tseslint from 'typescript-eslint';

// Dependency rule: only the Tauri packages may import @tauri-apps/*.
const noTauri = {
  'no-restricted-imports': ['error', { patterns: [{ group: ['@tauri-apps/*'], message: 'Only packages/platform-tauri and apps/tauri may import @tauri-apps/*.' }] }],
};

export default tseslint.config(
  { ignores: ['**/dist/**', '**/target/**', '**/node_modules/**', 'protocol/**', 'spec/**', '.claude/**'] },
  ...tseslint.configs.recommended,
  { files: ['**/*.ts'], rules: noTauri },
  { files: ['packages/platform-tauri/**', 'apps/tauri/**'], rules: { 'no-restricted-imports': 'off' } },
);
