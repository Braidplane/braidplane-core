-- PostgreSQL test database initialization
-- This script creates sample tables with various data types for testing schema introspection

-- Users table with common data types
CREATE TABLE users (
    id SERIAL PRIMARY KEY,
    email VARCHAR(255) NOT NULL UNIQUE,
    full_name VARCHAR(200) NOT NULL,
    age INTEGER,
    is_active BOOLEAN DEFAULT true,
    balance NUMERIC(10, 2),
    bio TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Products table with more variety
CREATE TABLE products (
    id SERIAL PRIMARY KEY,
    sku VARCHAR(50) NOT NULL UNIQUE,
    name VARCHAR(200) NOT NULL,
    description TEXT,
    price NUMERIC(12, 2) NOT NULL,
    stock_quantity INTEGER DEFAULT 0,
    is_available BOOLEAN DEFAULT true,
    category VARCHAR(100),
    tags TEXT[],
    metadata JSONB,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Orders table with foreign keys
CREATE TABLE orders (
    id SERIAL PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    order_number VARCHAR(50) NOT NULL UNIQUE,
    total_amount NUMERIC(12, 2) NOT NULL,
    status VARCHAR(50) DEFAULT 'pending',
    shipped_at TIMESTAMP,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Order items table (many-to-many relationship)
CREATE TABLE order_items (
    id SERIAL PRIMARY KEY,
    order_id INTEGER NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    product_id INTEGER NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
    quantity INTEGER NOT NULL,
    unit_price NUMERIC(12, 2) NOT NULL,
    subtotal NUMERIC(12, 2) NOT NULL
);

-- Insert sample data
INSERT INTO users (email, full_name, age, is_active, balance, bio) VALUES
    ('alice@example.com', 'Alice Smith', 28, true, 1250.50, 'Software engineer'),
    ('bob@example.com', 'Bob Johnson', 35, true, 750.00, 'Product manager'),
    ('charlie@example.com', 'Charlie Brown', 42, false, 0.00, 'Consultant');

INSERT INTO products (sku, name, description, price, stock_quantity, is_available, category, tags, metadata) VALUES
    ('LAPTOP-001', 'Professional Laptop', 'High-performance laptop for developers', 1299.99, 15, true, 'Electronics',
     ARRAY['laptop', 'computer', 'electronics'], '{"brand": "TechCo", "warranty_years": 3}'::jsonb),
    ('MOUSE-001', 'Wireless Mouse', 'Ergonomic wireless mouse', 29.99, 50, true, 'Accessories',
     ARRAY['mouse', 'wireless', 'accessories'], '{"brand": "PeripheralPro", "warranty_years": 1}'::jsonb),
    ('DESK-001', 'Standing Desk', 'Adjustable height standing desk', 499.99, 8, true, 'Furniture',
     ARRAY['desk', 'furniture', 'office'], '{"brand": "OfficePro", "max_height_cm": 120}'::jsonb);

INSERT INTO orders (user_id, order_number, total_amount, status) VALUES
    (1, 'ORD-2026-001', 1329.98, 'shipped'),
    (2, 'ORD-2026-002', 529.98, 'pending');

INSERT INTO order_items (order_id, product_id, quantity, unit_price, subtotal) VALUES
    (1, 1, 1, 1299.99, 1299.99),
    (1, 2, 1, 29.99, 29.99),
    (2, 3, 1, 499.99, 499.99),
    (2, 2, 1, 29.99, 29.99);
