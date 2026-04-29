import './DifficultySelector.css';

/**
 * DifficultySelector — Compact difficulty toggle for in-game use.
 *
 * Props:
 *  - difficulty : string — current difficulty ('easy'|'medium'|'hard')
 *  - onChange   : (difficulty) => void
 *  - disabled   : boolean — disable changes mid-game
 */
export default function DifficultySelector({ difficulty, onChange, disabled }) {
  const options = [
    { key: 'easy', label: 'Easy' },
    { key: 'medium', label: 'Medium' },
    { key: 'hard', label: 'Hard' },
  ];

  return (
    <div className="diff-selector" role="radiogroup" aria-label="Difficulty">
      {options.map((opt) => (
        <button
          key={opt.key}
          className={`diff-selector__btn ${
            difficulty === opt.key ? 'diff-selector__btn--active' : ''
          }`}
          role="radio"
          aria-checked={difficulty === opt.key}
          disabled={disabled}
          onClick={() => onChange(opt.key)}
        >
          {opt.label}
        </button>
      ))}
    </div>
  );
}
