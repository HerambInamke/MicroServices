-- One logical database per service (runs once, on first container init)
CREATE DATABASE auth_db;
CREATE DATABASE catalog_db;
CREATE DATABASE order_db;
CREATE DATABASE media_db;
