CREATE TABLE repositories(
    id UUID PRIMARY KEY,
    installation_id UUID NOT NULL,
    github_repo_id BIGINT NOT NULL UNIQUE,
    owner TEXT NOT NULL,
    name TEXT NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()


    CONSTRAINT fk_repositories_installation
        FOREIGN KEY (installation_id)
        REFERENCES installations(id)

    CONSTRAINT uq_repositories_owner_name
        UNIQUE (owner,name)
)