; Variables
(identifier) @variable
(for_stmt variable: (identifier) @variable)

; Comments
(comment) @comment
(shebang) @comment.doc

; Keywords
[
  "amends"
  "import"
  "as"
  "config"
  "target"
] @keyword

[
  "if"
  "then"
  "else"
  "for"
  "in"
] @keyword.control

; Operators
[
  "+"
  "-"
  "*"
  "/"
  "%"
  "=="
  "!="
  "<"
  "<="
  ">"
  ">="
  "&&"
  "||"
  "!"
  "="
] @operator

; Punctuation
[
  "{"
  "}"
  "["
  "]"
  "[]"
  "("
  ")"
] @punctuation.bracket

[
  ","
  "."
  ";"
] @punctuation.delimiter

; Types
(primitive_type) @type.builtin
(type_annotation (identifier) @type)
(qualified_type
  package: (identifier) @namespace
  name: (identifier) @type)

; Declarations
(target_decl name: (identifier) @function.definition)
(config_decl name: (identifier) @type.definition)
(field_decl name: (identifier) @property)
(import_decl alias: (identifier) @namespace)

; Member access & struct amending
(selector_expr field: (identifier) @property)
(struct_amend_expr type: (identifier) @type)
(struct_amend_expr
  type: (selector_expr
    operand: (identifier) @namespace
    field: (identifier) @type))

; Calls & built-ins
(call_expr function: (identifier) @function)
(call_expr function: (selector_expr field: (identifier) @function.method))

((call_expr
  function: (identifier) @function.builtin)
  (#any-of? @function.builtin
    "sh" "exec" "capture" "try_sh" "rm" "mkdir" "env" "glob" "exists" "print" "fail"))

; Literals & String Interpolation
(string_literal) @string
(multiline_string_literal) @string
(escape_sequence) @string.escape
(interpolation "\\(" @punctuation.special ")" @punctuation.special) @embedded
(int_literal) @number
(float_literal) @number
(bool_literal) @boolean
