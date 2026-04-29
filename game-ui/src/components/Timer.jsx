import { useState, useEffect, useRef } from 'react';
import './Timer.css';

/**
 * Timer — Displays elapsed game time in mm:ss format.
 *
 * Props:
 *  - running : boolean — whether the timer is active
 *  - resetSignal : any — when changed, resets the timer to 00:00
 *  - onTimeUpdate : (seconds) => void — called every second with current elapsed
 */
export default function Timer({ running, resetSignal, onTimeUpdate }) {
  const [elapsed, setElapsed] = useState(0);
  const intervalRef = useRef(null);

  // Reset timer when resetSignal changes
  useEffect(() => {
    setElapsed(0);
  }, [resetSignal]);

  // Start / stop interval based on running prop
  useEffect(() => {
    if (running) {
      intervalRef.current = setInterval(() => {
        setElapsed((prev) => {
          const next = prev + 1;
          onTimeUpdate?.(next);
          return next;
        });
      }, 1000);
    } else {
      if (intervalRef.current) {
        clearInterval(intervalRef.current);
        intervalRef.current = null;
      }
    }

    return () => {
      if (intervalRef.current) {
        clearInterval(intervalRef.current);
        intervalRef.current = null;
      }
    };
  }, [running, onTimeUpdate]);

  const minutes = Math.floor(elapsed / 60);
  const seconds = elapsed % 60;
  const display = `${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}`;

  return (
    <div className="timer" role="timer" aria-label={`Elapsed time: ${display}`}>
      <span className="timer__icon" aria-hidden="true">⏱</span>
      <span className="timer__value">{display}</span>
    </div>
  );
}
