-- MySQL test database initialization
-- This script creates sample tables with various data types for testing schema introspection

-- Users table with common data types
CREATE TABLE users (
    id INT AUTO_INCREMENT PRIMARY KEY,
    email VARCHAR(255) NOT NULL UNIQUE,
    full_name VARCHAR(200) NOT NULL,
    age INT,
    is_active BOOLEAN DEFAULT TRUE,
    balance DECIMAL(10, 2),
    bio TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_email (email)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Products table with more variety
CREATE TABLE products (
    id INT AUTO_INCREMENT PRIMARY KEY,
    sku VARCHAR(50) NOT NULL UNIQUE,
    name VARCHAR(200) NOT NULL,
    description TEXT,
    price DECIMAL(12, 2) NOT NULL,
    stock_quantity INT DEFAULT 0,
    is_available BOOLEAN DEFAULT TRUE,
    category VARCHAR(100),
    metadata JSON,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_sku (sku),
    INDEX idx_category (category)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Orders table with foreign keys
CREATE TABLE orders (
    id INT AUTO_INCREMENT PRIMARY KEY,
    user_id INT NOT NULL,
    order_number VARCHAR(50) NOT NULL UNIQUE,
    total_amount DECIMAL(12, 2) NOT NULL,
    status VARCHAR(50) DEFAULT 'pending',
    shipped_at TIMESTAMP NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    INDEX idx_order_number (order_number),
    INDEX idx_user_id (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Order items table (many-to-many relationship)
CREATE TABLE order_items (
    id INT AUTO_INCREMENT PRIMARY KEY,
    order_id INT NOT NULL,
    product_id INT NOT NULL,
    quantity INT NOT NULL,
    unit_price DECIMAL(12, 2) NOT NULL,
    subtotal DECIMAL(12, 2) NOT NULL,
    FOREIGN KEY (order_id) REFERENCES orders(id) ON DELETE CASCADE,
    FOREIGN KEY (product_id) REFERENCES products(id) ON DELETE RESTRICT,
    INDEX idx_order_id (order_id),
    INDEX idx_product_id (product_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Insert sample data
CREATE USER 'metadata_reader'@'%' IDENTIFIED BY '';
GRANT SELECT ON braidplane.users TO 'metadata_reader'@'%';

INSERT INTO users (id, email, full_name, age, is_active, balance, bio) VALUES
    (101, 'alice.mysql@example.com', 'Alice Smith MySQL', 28, TRUE, 1250.50, 'Software engineer'),
    (102, 'bob.mysql@example.com', 'Bob Johnson MySQL', 35, TRUE, 750.00, 'Product manager'),
    (103, 'charlie.mysql@example.com', 'Charlie Brown MySQL', 42, FALSE, 0.00, 'Consultant');

INSERT INTO products (sku, name, description, price, stock_quantity, is_available, category, metadata) VALUES
    ('LAPTOP-001', 'Professional Laptop', 'High-performance laptop for developers', 1299.99, 15, TRUE, 'Electronics',
     '{"brand": "TechCo", "warranty_years": 3}'),
    ('MOUSE-001', 'Wireless Mouse', 'Ergonomic wireless mouse', 29.99, 50, TRUE, 'Accessories',
     '{"brand": "PeripheralPro", "warranty_years": 1}'),
    ('DESK-001', 'Standing Desk', 'Adjustable height standing desk', 499.99, 8, TRUE, 'Furniture',
     '{"brand": "OfficePro", "max_height_cm": 120}');

INSERT INTO orders (user_id, order_number, total_amount, status) VALUES
    (101, 'ORD-2026-001', 1329.98, 'shipped'),
    (102, 'ORD-2026-002', 529.98, 'pending');

INSERT INTO order_items (order_id, product_id, quantity, unit_price, subtotal) VALUES
    (1, 1, 1, 1299.99, 1299.99),
    (1, 2, 1, 29.99, 29.99),
    (2, 3, 1, 499.99, 499.99),
    (2, 2, 1, 29.99, 29.99);
