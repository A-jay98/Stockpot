# Third-party notices

## Food icons

Sprites in `src/ui/food-icons/sprites/` are generated (`pnpm icons`) from these packages,
which are devDependencies only (nothing is fetched at runtime). The same notices ship with the
app at `/licenses.txt` (linked from the account sheet) and in each sprite's header comment.

| Package                                    | Version                      | Source                              | Licence |
| ------------------------------------------ | ---------------------------- | ----------------------------------- | ------- |
| `@iconify-json/fluent-emoji-high-contrast` | 1.2.6                        | Fluent Emoji, Microsoft Corporation | MIT     |
| `@iconify-json/fluent-emoji-flat`          | 1.2.6                        | Fluent Emoji, Microsoft Corporation | MIT     |
| `@iconify-json/tabler`                     | 1.2.47 (Tabler Icons 3.52.0) | Paweł Kuna                          | MIT     |

The iconify-json packages declare MIT in `info.json` (`license.spdx`). Licence texts below are
verbatim from the upstream repositories (`src/ui/food-icons/licenses/`).

### Fluent Emoji (https://github.com/microsoft/fluentui-emoji)

```text
    MIT License

    Copyright (c) Microsoft Corporation.

    Permission is hereby granted, free of charge, to any person obtaining a copy
    of this software and associated documentation files (the "Software"), to deal
    in the Software without restriction, including without limitation the rights
    to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
    copies of the Software, and to permit persons to whom the Software is
    furnished to do so, subject to the following conditions:

    The above copyright notice and this permission notice shall be included in all
    copies or substantial portions of the Software.

    THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
    IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
    FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
    AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
    LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
    OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
    SOFTWARE
```

### Tabler Icons (https://github.com/tabler/tabler-icons)

```text
MIT License

Copyright (c) 2020-2026 Paweł Kuna

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

## Store logos

`src/features/stores/marks.gen.ts` holds single-path logo glyphs copied from
[simple-icons](https://github.com/simple-icons/simple-icons) 16.38.1 (`icons/<slug>.svg`), whose
icon data is released under CC0-1.0. The logos are trademarks of their owners and are shown only
to identify a store the household named; stores without a glyph get a monogram in brand colours.
