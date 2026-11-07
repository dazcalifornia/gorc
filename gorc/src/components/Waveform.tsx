import { useEffect, useRef } from 'react'
import type { Waveform as WaveBuffer } from '../lib/useConnector'

interface Props {
  /** Read fresh each frame: the buffer is mutated in place by the hook. */
  read: () => WaveBuffer | undefined
  label: string
}

/**
 * Waveform draws the plethysmogram on a canvas from the ring buffer.
 *
 * It animates off requestAnimationFrame and reads the buffer directly, so a
 * 60 Hz waveform costs no React renders at all.
 */
export function Waveform({ read, label }: Props) {
  const canvasRef = useRef<HTMLCanvasElement>(null)

  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas) return
    const ctx = canvas.getContext('2d')
    if (!ctx) return

    let frame = 0
    const draw = () => {
      frame = requestAnimationFrame(draw)
      const buf = read()

      // Match the backing store to the CSS size so the trace stays crisp.
      const dpr = window.devicePixelRatio || 1
      const w = canvas.clientWidth
      const h = canvas.clientHeight
      if (canvas.width !== w * dpr || canvas.height !== h * dpr) {
        canvas.width = w * dpr
        canvas.height = h * dpr
      }
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
      ctx.clearRect(0, 0, w, h)

      const style = getComputedStyle(canvas)
      ctx.strokeStyle = style.getPropertyValue('--trace').trim() || '#38bdf8'
      ctx.lineWidth = 1.5
      ctx.lineJoin = 'round'

      if (!buf || buf.count < 2) {
        ctx.globalAlpha = 0.35
        ctx.beginPath()
        ctx.moveTo(0, h / 2)
        ctx.lineTo(w, h / 2)
        ctx.stroke()
        ctx.globalAlpha = 1
        return
      }

      // Oldest sample first: walk the ring from the tail.
      const n = buf.count
      const start = (buf.head - n + buf.data.length) % buf.data.length
      const pad = 4
      ctx.beginPath()
      for (let i = 0; i < n; i++) {
        const v = buf.data[(start + i) % buf.data.length] / 127 // protocol range
        const x = (i / (n - 1)) * w
        const y = h - pad - v * (h - pad * 2)
        if (i === 0) ctx.moveTo(x, y)
        else ctx.lineTo(x, y)
      }
      ctx.stroke()
    }

    frame = requestAnimationFrame(draw)
    return () => cancelAnimationFrame(frame)
  }, [read])

  return (
    <figure className="waveform">
      <canvas ref={canvasRef} aria-label={label} role="img" />
      <figcaption>{label}</figcaption>
    </figure>
  )
}
