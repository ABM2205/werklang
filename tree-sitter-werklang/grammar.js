/**
 * @file Tree-sitter grammar for Werklang v2 (Go + Pkl hybrid scripts)
 * @license MIT
 */

/// <reference types="tree-sitter-cli/dsl" />
// @ts-check

const GO = require('tree-sitter-go/grammar');

const terminator = choice(/\n/, ';', '\0');

module.exports = grammar(GO, {
  name: 'werklang',

  externals: $ => [
    $.config_keyword,
    $.pkl_body,
  ],

  rules: {
    source_file: $ => seq(
      optional($.shebang),
      repeat(choice(
        seq($.config_declaration, terminator),
        seq($._statement, terminator),
        seq($._top_level_declaration, terminator),
      )),
      optional(choice(
        $.config_declaration,
        $._top_level_declaration,
        $._statement,
      )),
    ),

    shebang: _ => token(seq('#!', /[^\n]*/)),

    config_declaration: $ => seq(
      field('keyword', $.config_keyword),
      optional(field('name', $.identifier)),
      '{',
      optional(field('body', $.pkl_body)),
      '}',
    ),
  },
});
