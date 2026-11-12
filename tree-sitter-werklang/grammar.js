/**
 * @file Tree-sitter grammar for Werklang (.werk / Werkfile)
 * @license MIT
 */

/// <reference types="tree-sitter-cli/dsl" />
// @ts-check

const PREC = {
  CONDITIONAL: 1,
  OR: 2,
  AND: 3,
  COMPARATIVE: 4,
  ADDITIVE: 5,
  MULTIPLICATIVE: 6,
  UNARY: 7,
  POSTFIX: 8,
};

/**
 * @param {RuleOrLiteral} rule
 */
function commaSep(rule) {
  return optional(seq(rule, repeat(seq(',', rule)), optional(',')));
}

/**
 * @param {RuleOrLiteral} item
 * @param {RuleOrLiteral} terminator
 */
function terminatedList(item, terminator) {
  return seq(item, repeat(seq(terminator, item)), optional(terminator));
}

/**
 * Parameterized unary/binary/postfix/conditional rules.
 * When `allowStruct` is false (inside `if <cond>` and `for x in <iter>` headers),
 * unparenthesized `struct_amend_expr` (`Expr { ... }`) is disallowed so `{`
 * unambiguously begins the statement block.
 *
 * @param {GrammarSymbols<any>} $
 * @param {boolean} allowStruct
 */
function makeBinaryRule($, allowStruct) {
  const expr = allowStruct ? $._expression : $._expr_no_struct;
  /** @type {[number, RuleOrLiteral][]} */
  const table = [
    [PREC.OR, '||'],
    [PREC.AND, '&&'],
    [PREC.COMPARATIVE, choice('==', '!=', '<', '<=', '>', '>=')],
    [PREC.ADDITIVE, choice('+', '-')],
    [PREC.MULTIPLICATIVE, choice('*', '/', '%')],
  ];

  return choice(...table.map(([precedence, operator]) =>
    prec.left(precedence, seq(
      field('left', expr),
      field('operator', operator),
      field('right', expr),
    )),
  ));
}

module.exports = grammar({
  name: 'werklang',

  extras: $ => [
    /\s/,
    $.comment,
  ],

  word: $ => $.identifier,

  rules: {
    source_file: $ => seq(
      optional($.shebang),
      optional(choice(
        seq(
          $.amends_clause,
          repeat(seq($._terminator, $._top_level_item)),
          optional($._terminator),
        ),
        terminatedList($._top_level_item, $._terminator),
      )),
    ),

    shebang: _ => token(seq('#!', /[^\n]*/)),

    comment: _ => token(seq('//', /[^\n]*/)),

    _terminator: _ => choice('\n', ';'),

    _top_level_item: $ => choice(
      $.import_decl,
      $.config_decl,
      $.target_decl,
      $.field_decl,
      $._statement,
    ),

    amends_clause: $ => seq(
      'amends',
      field('uri', $.string_literal),
    ),

    import_decl: $ => seq(
      'import',
      field('uri', $.string_literal),
      optional(seq('as', field('alias', $.identifier))),
    ),

    config_decl: $ => seq(
      'config',
      field('name', $.identifier),
      field('body', $.block),
    ),

    target_decl: $ => seq(
      'target',
      field('name', $.identifier),
      field('body', $.block),
    ),

    block: $ => seq(
      '{',
      optional(terminatedList($._block_item, $._terminator)),
      '}',
    ),

    _block_item: $ => choice(
      $.field_decl,
      $._statement,
    ),

    field_decl: $ => seq(
      field('name', $.identifier),
      choice(
        seq(
          field('type', $.type_annotation),
          optional(seq('=', field('value', $._expression))),
        ),
        seq('=', field('value', $._expression)),
      ),
    ),

    type_annotation: $ => choice(
      $.primitive_type,
      $.slice_type,
      $.qualified_type,
      $.identifier,
    ),

    primitive_type: _ => choice('int', 'float', 'string', 'bool'),

    slice_type: $ => seq('[]', $.type_annotation),

    qualified_type: $ => seq(
      field('package', $.identifier),
      '.',
      field('name', $.identifier),
    ),

    _statement: $ => choice(
      $.if_stmt,
      $.for_stmt,
      $.expression_stmt,
    ),

    if_stmt: $ => seq(
      'if',
      field('condition', $._expr_no_struct),
      field('consequence', $.block),
      optional(seq(
        'else',
        field('alternative', choice($.if_stmt, $.block)),
      )),
    ),

    for_stmt: $ => seq(
      'for',
      field('variable', $.identifier),
      'in',
      field('iterable', $._expr_no_struct),
      field('body', $.block),
    ),

    expression_stmt: $ => $._expression,

    _expression: $ => choice(
      $._atom_expr,
      $.if_expr,
      $.unary_expr,
      $.binary_expr,
      $.call_expr,
      $.selector_expr,
      $.index_expr,
      $.struct_amend_expr,
    ),

    _expr_no_struct: $ => choice(
      $._atom_expr,
      alias($._if_expr_no_struct, $.if_expr),
      alias($._unary_expr_no_struct, $.unary_expr),
      alias($._binary_expr_no_struct, $.binary_expr),
      alias($._call_expr_no_struct, $.call_expr),
      alias($._selector_expr_no_struct, $.selector_expr),
      alias($._index_expr_no_struct, $.index_expr),
    ),

    _atom_expr: $ => choice(
      $.identifier,
      $.int_literal,
      $.float_literal,
      $.bool_literal,
      $.string_literal,
      $.multiline_string_literal,
      $.slice_literal,
      $.parenthesized_expr,
    ),

    if_expr: $ => prec.right(PREC.CONDITIONAL, seq(
      'if',
      field('condition', $._expr_no_struct),
      'then',
      field('consequence', $._expression),
      'else',
      field('alternative', $._expression),
    )),

    _if_expr_no_struct: $ => prec.right(PREC.CONDITIONAL, seq(
      'if',
      field('condition', $._expr_no_struct),
      'then',
      field('consequence', $._expression),
      'else',
      field('alternative', $._expr_no_struct),
    )),

    unary_expr: $ => prec.right(PREC.UNARY, seq(
      field('operator', choice('!', '-')),
      field('operand', $._expression),
    )),

    _unary_expr_no_struct: $ => prec.right(PREC.UNARY, seq(
      field('operator', choice('!', '-')),
      field('operand', $._expr_no_struct),
    )),

    binary_expr: $ => makeBinaryRule($, true),

    _binary_expr_no_struct: $ => makeBinaryRule($, false),

    call_expr: $ => prec.left(PREC.POSTFIX, seq(
      field('function', $._expression),
      field('arguments', $.argument_list),
    )),

    _call_expr_no_struct: $ => prec.left(PREC.POSTFIX, seq(
      field('function', $._expr_no_struct),
      field('arguments', $.argument_list),
    )),

    argument_list: $ => seq(
      '(',
      commaSep($._expression),
      ')',
    ),

    selector_expr: $ => prec.left(PREC.POSTFIX, seq(
      field('operand', $._expression),
      '.',
      field('field', $.identifier),
    )),

    _selector_expr_no_struct: $ => prec.left(PREC.POSTFIX, seq(
      field('operand', $._expr_no_struct),
      '.',
      field('field', $.identifier),
    )),

    index_expr: $ => prec.left(PREC.POSTFIX, seq(
      field('operand', $._expression),
      '[',
      field('index', $._expression),
      ']',
    )),

    _index_expr_no_struct: $ => prec.left(PREC.POSTFIX, seq(
      field('operand', $._expr_no_struct),
      '[',
      field('index', $._expression),
      ']',
    )),

    struct_amend_expr: $ => prec.left(PREC.POSTFIX, seq(
      field('type', $._expression),
      '{',
      optional(terminatedList($.field_decl, choice('\n', ',', ';'))),
      '}',
    )),

    slice_literal: $ => seq('[', commaSep($._expression), ']'),

    parenthesized_expr: $ => seq('(', $._expression, ')'),

    string_literal: $ => seq(
      '"',
      repeat(choice(
        $.string_content,
        $.escape_sequence,
        $.interpolation,
      )),
      alias(token.immediate('"'), '"'),
    ),

    string_content: _ => token.immediate(prec(1, /[^"\\\n]+/)),

    escape_sequence: _ => token.immediate(seq(
      '\\',
      choice(
        /["\\ntr0bfv]/,
        /x[0-9a-fA-F]{2}/,
        /u[0-9a-fA-F]{4}/,
        /U[0-9a-fA-F]{8}/,
      ),
    )),

    interpolation: $ => seq(
      alias(token.immediate('\\('), '\\('),
      $._expression,
      ')',
    ),

    multiline_string_literal: _ => token(seq(
      '"""',
      repeat(choice(
        /[^"]/,
        /"[^"]/,
        /""[^"]/,
      )),
      '"""',
    )),

    int_literal: _ => token(choice(
      /0[xX][0-9a-fA-F][0-9a-fA-F_]*/,
      /0[oO][0-7][0-7_]*/,
      /0[bB][01][01_]*/,
      /[0-9][0-9_]*/,
    )),

    float_literal: _ => token(choice(
      /[0-9][0-9_]*\.[0-9][0-9_]*([eE][+-]?[0-9][0-9_]*)?/,
      /[0-9][0-9_]*[eE][+-]?[0-9][0-9_]*/,
    )),

    bool_literal: _ => choice('true', 'false'),

    identifier: _ => /[a-zA-Z_][a-zA-Z0-9_]*/,
  },
});
