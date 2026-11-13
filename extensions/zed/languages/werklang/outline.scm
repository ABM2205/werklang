(config_declaration
  keyword: (config_keyword) @context
  name: (identifier)? @name) @item

(function_declaration
  "func" @context
  name: (identifier) @name) @item

(method_declaration
  "func" @context
  receiver: (parameter_list) @context.extra
  name: (field_identifier) @name) @item

(type_declaration
  (type_spec
    name: (type_identifier) @name)) @item
