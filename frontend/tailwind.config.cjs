/** @type {import('tailwindcss').Config} */
module.exports = {
    content: [
        './views/**/*.templ',
        './assets/js/**/*.js',
        './**/*.go',
    ],
    theme: {
        extend: {},
    },
    plugins: [require('daisyui')],
};