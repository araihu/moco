CREATE TABLE authorization_role_bindings (
    principal_id TEXT NOT NULL CHECK (length(principal_id) BETWEEN 1 AND 128),
    role TEXT NOT NULL CHECK (length(role) BETWEEN 1 AND 128),
    domain TEXT NOT NULL CHECK (length(domain) BETWEEN 1 AND 128),
    PRIMARY KEY (principal_id, role, domain)
);

CREATE TABLE authorization_policies (
    subject TEXT NOT NULL CHECK (length(subject) BETWEEN 1 AND 128),
    domain TEXT NOT NULL CHECK (length(domain) BETWEEN 1 AND 128),
    path TEXT NOT NULL CHECK (length(path) BETWEEN 7 AND 512),
    method TEXT NOT NULL CHECK (length(method) BETWEEN 1 AND 16),
    PRIMARY KEY (subject, domain, path, method)
);

CREATE INDEX authorization_role_bindings_domain_idx
    ON authorization_role_bindings (domain, principal_id);

CREATE INDEX authorization_policies_subject_domain_idx
    ON authorization_policies (subject, domain);
