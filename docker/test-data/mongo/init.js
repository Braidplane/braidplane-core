// MongoDB test database initialization
// This script creates sample collections with various data types for testing schema introspection

// Switch to the Braidplane fixture database.
db = db.getSiblingDB('braidplane');

// Users collection with common BSON types
db.users.insertMany([
    {
        email: 'alice@example.com',
        fullName: 'Alice Smith',
        age: 28,
        isActive: true,
        balance: 1250.50,
        bio: 'Software engineer',
        createdAt: new Date('2026-01-15T10:30:00Z'),
        updatedAt: new Date('2026-02-20T14:22:00Z'),
        preferences: {
            theme: 'dark',
            notifications: true,
            language: 'en'
        },
        tags: ['developer', 'early-adopter']
    },
    {
        email: 'bob@example.com',
        fullName: 'Bob Johnson',
        age: 35,
        isActive: true,
        balance: 750.00,
        bio: 'Product manager',
        createdAt: new Date('2026-01-20T09:15:00Z'),
        updatedAt: new Date('2026-02-18T11:45:00Z'),
        preferences: {
            theme: 'light',
            notifications: false,
            language: 'en'
        },
        tags: ['manager', 'premium']
    },
    {
        email: 'charlie@example.com',
        fullName: 'Charlie Brown',
        age: 42,
        isActive: false,
        balance: 0.00,
        bio: 'Consultant',
        createdAt: new Date('2025-12-01T08:00:00Z'),
        updatedAt: new Date('2026-01-15T16:30:00Z'),
        preferences: {
            theme: 'dark',
            notifications: true,
            language: 'fr'
        },
        tags: ['consultant']
    }
]);

// Products collection with varied types
db.products.insertMany([
    {
        sku: 'LAPTOP-001',
        name: 'Professional Laptop',
        description: 'High-performance laptop for developers',
        price: 1299.99,
        stockQuantity: 15,
        isAvailable: true,
        category: 'Electronics',
        tags: ['laptop', 'computer', 'electronics'],
        metadata: {
            brand: 'TechCo',
            warrantyYears: 3,
            specs: {
                cpu: 'Intel i7',
                ram: '16GB',
                storage: '512GB SSD'
            }
        },
        createdAt: new Date('2026-01-10T12:00:00Z')
    },
    {
        sku: 'MOUSE-001',
        name: 'Wireless Mouse',
        description: 'Ergonomic wireless mouse',
        price: 29.99,
        stockQuantity: 50,
        isAvailable: true,
        category: 'Accessories',
        tags: ['mouse', 'wireless', 'accessories'],
        metadata: {
            brand: 'PeripheralPro',
            warrantyYears: 1,
            specs: {
                dpi: 1600,
                buttons: 5
            }
        },
        createdAt: new Date('2026-01-12T14:30:00Z')
    },
    {
        sku: 'DESK-001',
        name: 'Standing Desk',
        description: 'Adjustable height standing desk',
        price: 499.99,
        stockQuantity: 8,
        isAvailable: true,
        category: 'Furniture',
        tags: ['desk', 'furniture', 'office'],
        metadata: {
            brand: 'OfficePro',
            maxHeightCm: 120,
            specs: {
                width: '160cm',
                depth: '80cm',
                weight: '45kg'
            }
        },
        createdAt: new Date('2026-01-08T09:00:00Z')
    }
]);

// Orders collection with references and embedded documents
db.orders.insertMany([
    {
        orderNumber: 'ORD-2026-001',
        userId: ObjectId(),
        userEmail: 'alice@example.com',
        totalAmount: 1329.98,
        status: 'shipped',
        items: [
            {
                productSku: 'LAPTOP-001',
                productName: 'Professional Laptop',
                quantity: 1,
                unitPrice: 1299.99,
                subtotal: 1299.99
            },
            {
                productSku: 'MOUSE-001',
                productName: 'Wireless Mouse',
                quantity: 1,
                unitPrice: 29.99,
                subtotal: 29.99
            }
        ],
        shippedAt: new Date('2026-02-15T10:00:00Z'),
        createdAt: new Date('2026-02-10T14:30:00Z')
    },
    {
        orderNumber: 'ORD-2026-002',
        userId: ObjectId(),
        userEmail: 'bob@example.com',
        totalAmount: 529.98,
        status: 'pending',
        items: [
            {
                productSku: 'DESK-001',
                productName: 'Standing Desk',
                quantity: 1,
                unitPrice: 499.99,
                subtotal: 499.99
            },
            {
                productSku: 'MOUSE-001',
                productName: 'Wireless Mouse',
                quantity: 1,
                unitPrice: 29.99,
                subtotal: 29.99
            }
        ],
        shippedAt: null,
        createdAt: new Date('2026-02-18T09:15:00Z')
    }
]);

// Analytics collection with mixed types (to test type inference)
db.analytics.insertMany([
    {
        eventType: 'page_view',
        timestamp: new Date(),
        userId: 'user_123',
        value: 1  // integer
    },
    {
        eventType: 'purchase',
        timestamp: new Date(),
        userId: 'user_456',
        value: 99.99  // double
    },
    {
        eventType: 'signup',
        timestamp: new Date(),
        userId: 'user_789',
        value: null  // null
    }
]);

// Create indexes for better query performance
db.users.createIndex({ email: 1 }, { unique: true });
db.products.createIndex({ sku: 1 }, { unique: true });
db.products.createIndex({ category: 1 });
db.orders.createIndex({ orderNumber: 1 }, { unique: true });
db.orders.createIndex({ userEmail: 1 });
db.orders.createIndex({ status: 1 });

print('MongoDB test database initialized successfully');
print('Collections created: users, products, orders, analytics');
print('Documents inserted:');
print('  - users: ' + db.users.countDocuments());
print('  - products: ' + db.products.countDocuments());
print('  - orders: ' + db.orders.countDocuments());
print('  - analytics: ' + db.analytics.countDocuments());
