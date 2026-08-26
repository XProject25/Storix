/** Storix design system. Developed by X Project. */
export default {
  darkMode: 'class',
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  theme: {
    extend: {
      colors: {
        bg: 'rgb(var(--sx-bg) / <alpha-value>)',
        surface: 'rgb(var(--sx-surface) / <alpha-value>)',
        elevated: 'rgb(var(--sx-elevated) / <alpha-value>)',
        line: 'rgb(var(--sx-line) / <alpha-value>)',
        ink: 'rgb(var(--sx-ink) / <alpha-value>)',
        muted: 'rgb(var(--sx-muted) / <alpha-value>)',
        faint: 'rgb(var(--sx-faint) / <alpha-value>)',
        primary: 'rgb(var(--sx-primary) / <alpha-value>)',
        secondary: 'rgb(var(--sx-secondary) / <alpha-value>)',
        accent: 'rgb(var(--sx-accent) / <alpha-value>)',
        violet: 'rgb(var(--sx-violet) / <alpha-value>)',
        success: 'rgb(var(--sx-success) / <alpha-value>)',
        warning: 'rgb(var(--sx-warning) / <alpha-value>)',
        danger: 'rgb(var(--sx-danger) / <alpha-value>)',
      },
      fontFamily: {
        sans: ['Inter', 'ui-sans-serif', 'system-ui', 'Segoe UI', 'Roboto', 'Helvetica Neue', 'Arial', 'sans-serif'],
        mono: ['JetBrains Mono', 'ui-monospace', 'SFMono-Regular', 'Menlo', 'Consolas', 'monospace'],
      },
      borderRadius: {
        xl: '0.875rem',
        '2xl': '1.125rem',
        '3xl': '1.5rem',
      },
      boxShadow: {
        panel: '0 1px 2px rgb(0 0 0 / 0.28), 0 12px 32px -12px rgb(0 0 0 / 0.45)',
        pop: '0 8px 40px -8px rgb(0 0 0 / 0.55)',
        glow: '0 0 0 1px rgb(var(--sx-primary) / 0.35), 0 8px 30px -8px rgb(var(--sx-primary) / 0.35)',
      },
      transitionTimingFunction: {
        swift: 'cubic-bezier(0.22, 1, 0.36, 1)',
      },
      keyframes: {
        'fade-in': { from: { opacity: '0' }, to: { opacity: '1' } },
        'slide-up': { from: { opacity: '0', transform: 'translateY(8px)' }, to: { opacity: '1', transform: 'translateY(0)' } },
        'slide-in': { from: { opacity: '0', transform: 'translateX(16px)' }, to: { opacity: '1', transform: 'translateX(0)' } },
        shimmer: { '100%': { transform: 'translateX(100%)' } },
        'pulse-soft': { '0%,100%': { opacity: '1' }, '50%': { opacity: '0.55' } },
      },
      animation: {
        'fade-in': 'fade-in 160ms ease-out',
        'slide-up': 'slide-up 180ms cubic-bezier(0.22, 1, 0.36, 1)',
        'slide-in': 'slide-in 200ms cubic-bezier(0.22, 1, 0.36, 1)',
        shimmer: 'shimmer 1.4s infinite',
      },
    },
  },
  plugins: [],
}
