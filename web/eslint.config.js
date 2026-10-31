import tseslint from 'typescript-eslint';

export default tseslint.config(
  { ignores: ['**/dist/**', '**/*.d.ts', '**/playwright-report/**', '**/test-results/**'] },
  ...tseslint.configs.recommended,
);
