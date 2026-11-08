import reactHooks from 'eslint-plugin-react-hooks';
import tseslint from 'typescript-eslint';

export default tseslint.config(
  { ignores: ['dist/**', '**/*.d.ts'] },
  ...tseslint.configs.recommended,
  reactHooks.configs.flat.recommended,
  {
    // Partner strings are data: never turned into HTML.
    rules: {
      'no-restricted-syntax': [
        'error',
        { selector: "JSXAttribute[name.name='dangerouslySetInnerHTML']", message: 'No raw HTML from data.' },
      ],
    },
  },
);
