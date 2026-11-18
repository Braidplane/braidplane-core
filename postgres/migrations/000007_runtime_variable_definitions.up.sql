CREATE TABLE runtime_variable_definitions (
    organization_id integer NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    key varchar(100) NOT NULL,
    value_type text NOT NULL CHECK (value_type IN ('text', 'integer', 'boolean', 'date', 'uuid')),
    description varchar(2000),
    required boolean NOT NULL DEFAULT false,
    default_value varchar(4000),
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (organization_id, key)
);
