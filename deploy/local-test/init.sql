CREATE DATABASE IF NOT EXISTS echo_local_core CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;
CREATE DATABASE IF NOT EXISTS echo_local_whatsapp CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;

CREATE USER IF NOT EXISTS 'echo_local_core'@'%' IDENTIFIED BY 'echo-local-core-password';
CREATE USER IF NOT EXISTS 'echo_local_whatsapp'@'%' IDENTIFIED BY 'echo-local-whatsapp-password';

GRANT ALL PRIVILEGES ON echo_local_core.* TO 'echo_local_core'@'%';
GRANT ALL PRIVILEGES ON echo_local_whatsapp.* TO 'echo_local_whatsapp'@'%';
FLUSH PRIVILEGES;
