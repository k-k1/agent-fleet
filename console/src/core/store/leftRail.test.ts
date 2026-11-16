import { beforeEach, describe, expect, it } from "vitest";
import { useLeftRail } from "./leftRail.ts";

beforeEach(() => {
  useLeftRail.setState({ open: true, mode: "push", view: "sessions", swipeOverlay: false });
});

describe("left rail view", () => {
  it("pickView on another view shows it and keeps the rail open", () => {
    useLeftRail.getState().pickView("files");
    expect(useLeftRail.getState()).toMatchObject({ open: true, view: "files" });
  });

  it("pickView on the view on screen folds the rail and keeps the choice", () => {
    useLeftRail.getState().pickView("sessions");
    expect(useLeftRail.getState()).toMatchObject({ open: false, view: "sessions" });
  });

  it("pickView on any view while folded opens it rather than toggling shut", () => {
    useLeftRail.setState({ open: false });
    useLeftRail.getState().pickView("sessions");
    expect(useLeftRail.getState()).toMatchObject({ open: true, view: "sessions" });
  });

  it("showView never folds a rail that already shows the view", () => {
    useLeftRail.getState().showView("sessions");
    expect(useLeftRail.getState()).toMatchObject({ open: true, view: "sessions" });
    useLeftRail.setState({ open: false });
    useLeftRail.getState().showView("memo");
    expect(useLeftRail.getState()).toMatchObject({ open: true, view: "memo" });
  });
});
