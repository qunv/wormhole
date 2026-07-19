CREATE TABLE users (id BIGINT AUTO_INCREMENT PRIMARY KEY, email VARCHAR(255) NOT NULL, status VARCHAR(32) NOT NULL DEFAULT 'active', INDEX idx_email(email));
CREATE TABLE secrets (id BIGINT AUTO_INCREMENT PRIMARY KEY, value TEXT NOT NULL);
INSERT INTO users(email) VALUES ('one@example.com'), ('two@example.com');
CREATE USER IF NOT EXISTS 'codebridge'@'%' IDENTIFIED BY 'codebridge';
GRANT SELECT ON app.* TO 'codebridge'@'%';
CREATE USER IF NOT EXISTS 'codebridge_writer'@'%' IDENTIFIED BY 'codebridge_writer';
GRANT SELECT, UPDATE, DELETE ON app.* TO 'codebridge_writer'@'%';
FLUSH PRIVILEGES;
