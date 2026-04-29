import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import App from "../App";

describe("App", () => {
  it("renders the Hello component", () => {
    render(<App />);
    expect(screen.getByText("Hello, Brain!")).toBeInTheDocument();
  });

  it("renders the count button", () => {
    render(<App />);
    expect(screen.getByText(/Count is 0/)).toBeInTheDocument();
  });
});
