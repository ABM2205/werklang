(config_decl
  "config" @context
  name: (identifier) @name) @item

(config_decl
  body: (block
    (field_decl
      name: (identifier) @name) @item))

(target_decl
  "target" @context
  name: (identifier) @name) @item

(source_file
  (field_decl
    name: (identifier) @name) @item)
