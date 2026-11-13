; Inject Apple Pkl syntax highlighting into `config { ... }` bodies
((pkl_body) @injection.content
  (#set! injection.language "pkl"))
