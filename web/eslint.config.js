import tseslint from 'typescript-eslint';

export default tseslint.config(
  { ignores: ['**/dist/**', '**/*.d.ts'] },
  ...tseslint.configs.recommended,
);
