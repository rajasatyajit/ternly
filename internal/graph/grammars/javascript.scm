(function_declaration (identifier) @name) @definition.function
(method_definition (property_identifier) @name) @definition.method
(class_declaration (identifier) @name) @definition.class
(call_expression (identifier) @name) @reference.call
(call_expression (member_expression (property_identifier) @name)) @reference.call