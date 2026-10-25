-- Extra numeric host groups for a controller-managed microservice

ALTER TABLE controller_microservices ADD COLUMN supplemental_groups TEXT NOT NULL DEFAULT '[]'
