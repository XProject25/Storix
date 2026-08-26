// The Storix lockup. The mark is the supplied artwork, the wordmark is live
// text so it stays crisp and follows the active theme.
// Developed by X Project.

import clsx from 'clsx'

export interface LogoProps {
  size?: number
  wordmark?: boolean
  className?: string
  tagline?: string
}

export function Logo({ size = 30, wordmark = true, className, tagline }: LogoProps) {
  return (
    <div className={clsx('flex items-center gap-2.5 select-none', className)}>
      <img
        src="/storix-mark.png"
        width={size}
        height={size}
        alt=""
        className="drag-none shrink-0 object-contain"
        style={{ width: size, height: size }}
        draggable={false}
      />
      {wordmark && (
        <div className="leading-none">
          <div className="font-semibold tracking-tight" style={{ fontSize: size * 0.62 }}>
            <span className="text-ink">Stori</span>
            <span className="sx-gradient-text">x</span>
          </div>
          {tagline && <div className="mt-1 text-[11px] text-faint">{tagline}</div>}
        </div>
      )}
    </div>
  )
}

/** LogoMark is the icon on its own, for tight spaces. */
export function LogoMark({ size = 24, className }: { size?: number; className?: string }) {
  return (
    <img
      src="/storix-mark.png"
      width={size}
      height={size}
      alt="Storix"
      className={clsx('drag-none object-contain', className)}
      style={{ width: size, height: size }}
      draggable={false}
    />
  )
}
