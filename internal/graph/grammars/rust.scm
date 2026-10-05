(function_item (identifier) @name) @definition.function
(function_signature_item (identifier) @name) @definition.function
(struct_item (type_identifier) @name) @definition.type
(enum_item (type_identifier) @name) @definition.type
(trait_item (type_identifier) @name) @definition.type
(call_expression (identifier) @name) @reference.call
(call_expression (field_expression (field_identifier) @name)) @reference.call
(call_expression (scoped_identifier (identifier) @name)) @reference.call
(macro_invocation (identifier) @name) @reference.call