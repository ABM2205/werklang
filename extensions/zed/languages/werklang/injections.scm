((call_expr
  function: (identifier) @_fn
  arguments: (argument_list (string_literal (string_content) @injection.content)))
  (#any-of? @_fn "sh" "capture" "try_sh")
  (#set! injection.language "bash"))
